//go:build linux

// Package adminsettings serves administrator settings and restart intent.
// It owns request authorization, validation and response projection while the
// feature/admin/settings/live owns durable values and their live effects.
package adminsettings

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/heavycaffeiner/stowcloud/go/internal/feature/admin/settings/catalogue"
	"github.com/heavycaffeiner/stowcloud/go/internal/feature/admin/settings/check"
	"github.com/heavycaffeiner/stowcloud/go/internal/feature/admin/settings/live"
	"github.com/heavycaffeiner/stowcloud/go/internal/feature/auth"
	"github.com/heavycaffeiner/stowcloud/go/internal/platform/database/state"
	"github.com/heavycaffeiner/stowcloud/go/internal/platform/system/jail"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/apierr"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/handler"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/middleware"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/route"
)

const secretOIDCClient = "oidc_client_secret" //nolint:gosec // G101 flags this config key; it names stored secret material but is not secret material itself.

const restartGrace = 250 * time.Millisecond

// Deps contains only services and callbacks needed by these routes.
type Deps struct {
	State        *state.DB
	Auth         *auth.Service
	Settings     *live.Coordinator
	DataDir      string
	Hardening    jail.Policy
	Admin        func(*gin.Context) (int64, bool)
	UploadPatch  gin.HandlerFunc
	SMBAgentView func() *handler.SMBAgentView
	PublishSMB   func(context.Context)
	OnRestart    func()
	Logger       *slog.Logger
}

// NewHandlers returns the route-name to handler mapping consumed by app
// composition.
func NewHandlers(d Deps) map[string]gin.HandlerFunc {
	h := &handlers{d: d}
	return map[string]gin.HandlerFunc{
		"admin.settings.get":   h.get,
		"admin.settings.patch": h.patch,
		"admin.system.restart": h.restart,
	}
}

type handlers struct{ d Deps }

func (h *handlers) get(c *gin.Context) {
	if _, ok := h.d.Admin(c); !ok {
		return
	}
	stored, err := h.d.State.Settings(c.Request.Context())
	if err != nil {
		h.failKnown(c, err)
		return
	}
	if stored == nil {
		stored = map[string]any{}
	}
	values := h.d.Settings.Values(c.Request.Context())
	values.DataDir = h.d.DataDir
	h.json(c, http.StatusOK, handler.SettingsOf(catalogue.Of(values, stored), h.hopOf(c), h.d.SMBAgentView()))
}

func (h *handlers) patch(c *gin.Context) {
	if _, ok := h.d.Admin(c); !ok {
		return
	}
	section := c.Param("section")
	if section == "upload" {
		if h.d.UploadPatch == nil {
			h.refuse(c, apierr.Classified{Class: apierr.Unprocessable})
			return
		}
		h.d.UploadPatch(c)
		return
	}
	if !check.Known(section) {
		h.refuse(c, apierr.Classified{Class: apierr.Unprocessable})
		return
	}
	var body map[string]any
	if err := decode(c, &body); err != nil {
		h.refuse(c, apierr.Classified{Class: apierr.Malformed})
		return
	}
	if !h.extractSecrets(c, section, body) {
		return
	}
	findings := check.Section(check.Input{
		Section:   section,
		Body:      body,
		SelfHost:  check.HostOnly(string(c.Request.Host)),
		DataDir:   h.d.DataDir,
		HasSecret: section == "oidc" && h.d.Settings.HasConfigSecret(c.Request.Context(), secretOIDCClient),
		Lockout:   check.LockoutBlocks,
	})
	if handler.Blocking(findings) {
		h.json(c, http.StatusUnprocessableEntity, handler.ApplyOutcomeOf(false, false, false, findings))
		return
	}
	if err := h.d.State.MergeSettings(c.Request.Context(), section, body); err != nil {
		h.failKnown(c, err)
		return
	}
	restart := catalogue.RestartRequiredFor(section, body)
	if !restart {
		h.d.Settings.Load(c.Request.Context())
	}
	if section == "smb" && h.d.PublishSMB != nil {
		h.d.PublishSMB(c.Request.Context())
	}
	applied := !restart
	if pin := h.pinnedBindFinding(section, body); pin != nil {
		applied = false
		findings = append(findings, *pin)
	}
	out := handler.ApplyOutcomeOf(true, applied, restart, findings)
	if restart {
		uploads, jobs := h.activeWork(c)
		out = out.WithActiveWork(uploads, jobs)
	}
	h.json(c, http.StatusOK, out)
}

func (h *handlers) restart(c *gin.Context) {
	if _, ok := h.d.Admin(c); !ok {
		return
	}
	if h.d.Settings.WouldLoosenHardening(c.Request.Context(), h.d.Hardening) {
		h.refuse(c, apierr.Classified{Class: apierr.Conflict, Key: "system.hardening_cannot_loosen"})
		return
	}
	uploads, jobs := h.activeWork(c)
	h.json(c, http.StatusAccepted, restartResult{Restarting: true, ActiveUploads: uploads, ActiveJobs: jobs})
	if h.d.OnRestart != nil {
		time.AfterFunc(restartGrace, h.d.OnRestart)
	}
}

type restartResult struct {
	Restarting    bool `json:"restarting"`
	ActiveUploads int  `json:"active_uploads"`
	ActiveJobs    int  `json:"active_jobs"`
}

func (h *handlers) activeWork(c *gin.Context) (uploads, jobs int) {
	w, err := h.d.State.CountActiveWork(c.Request.Context())
	if err != nil {
		if h.d.Logger != nil {
			h.d.Logger.Warn("could not count the work a restart would interrupt", "error", err)
		}
		return 1, 0
	}
	return w.Uploads, w.Jobs
}

func (h *handlers) pinnedBindFinding(section string, body map[string]any) *check.Finding {
	if section != "network" || !h.d.Settings.BindPinned() {
		return nil
	}
	stored, named := body["bind"].(string)
	if !named || stored == "" {
		return nil
	}
	return &check.Finding{Section: section, Field: "bind", ReasonKey: "settings.bind_pinned_by_flag", Args: []string{"stored", stored}}
}

func (h *handlers) extractSecrets(c *gin.Context, section string, body map[string]any) bool {
	if section != "oidc" {
		return true
	}
	raw, present := body["client_secret"]
	if !present {
		return true
	}
	delete(body, "client_secret")
	plain, ok := raw.(string)
	if !ok {
		h.refuse(c, apierr.Classified{Class: apierr.Unprocessable})
		return false
	}
	if err := h.d.Settings.StoreConfigSecret(c.Request.Context(), secretOIDCClient, plain); err != nil {
		h.failKnown(c, err)
		return false
	}
	return true
}

func (h *handlers) hopOf(c *gin.Context) handler.HopView {
	peer, err := peerAddress(c.Request.RemoteAddr)
	client := middleware.ClientOf(c)
	hop := handler.HopView{Client: client.String(), ForwardedSeen: c.GetHeader("CF-Connecting-IP") != "" || c.GetHeader("X-Forwarded-For") != ""}
	if err != nil {
		return hop
	}
	hop.Peer = peer.String()
	hop.PeerTrusted = middleware.PeerTrusted(peer, h.d.Settings.TrustedProxies())
	return hop
}

func peerAddress(raw string) (netip.Addr, error) {
	host, _, err := net.SplitHostPort(raw)
	if err == nil {
		return netip.ParseAddr(host)
	}
	return netip.ParseAddr(raw)
}

func decode(c *gin.Context, into any) error {
	return middleware.DecodeJSON(middleware.LimitBody(c.Request.Body, route.BodyJSON), into)
}
func (h *handlers) json(c *gin.Context, status int, value any) { c.JSON(status, value) }
func (h *handlers) refuse(c *gin.Context, class apierr.Classified) {
	status, body := apierr.REST(class)
	h.json(c, status, body)
}
func (h *handlers) failKnown(c *gin.Context, err error) {
	middleware.SetCause(c, err)
	h.refuse(c, apierr.Classify(err, apierr.VisibilityKnown))
}
