//go:build linux

// Package adminsettings serves administrator settings and restart intent.
// It owns validation and response projection; the application supplies the
// settings coordinator through a narrow interface.
package admin

import (
	"context"
	"log/slog"
	"net/http"
	"net/netip"
	"time"

	"github.com/heavycaffeiner/stowcloud/backend/internal/auth"
	"github.com/heavycaffeiner/stowcloud/backend/internal/config"
	"github.com/heavycaffeiner/stowcloud/backend/internal/db/state"
	"github.com/heavycaffeiner/stowcloud/backend/internal/platform/system/jail"
	"github.com/heavycaffeiner/stowcloud/backend/internal/server/apierr"
	"github.com/heavycaffeiner/stowcloud/backend/internal/server/middleware"
)

const secretOIDCClient = "oidc_client_secret" //nolint:gosec // G101 flags this config key; it names stored secret material but is not secret material itself.

const restartGrace = 250 * time.Millisecond

// Settings is the application settings boundary used by these routes.
type Settings interface {
	Values(context.Context) config.Values
	Load(context.Context)
	HasConfigSecret(context.Context, string) bool
	WouldLoosenHardening(context.Context, jail.Policy) bool
	BindPinned() bool
	StoreConfigSecret(context.Context, string, string) error
	TrustedProxies() []netip.Prefix
}

// SettingsDeps contains only services and callbacks needed by these routes.
type SettingsDeps struct {
	State        *state.DB
	Auth         *auth.Service
	Settings     Settings
	DataDir      string
	Hardening    jail.Policy
	SMBAgentView func() *SMBAgentView
	PublishSMB   func(context.Context)
	OnRestart    func()
	Logger       *slog.Logger
}

// NewSettingsHandlers builds the administrator settings handlers.
func NewSettingsHandlers(d SettingsDeps) *SettingsHandlers {
	return &SettingsHandlers{d: d}
}

type SettingsHandlers struct{ d SettingsDeps }

type settingsGetInput struct {
	CFConnectingIP string `header:"CF-Connecting-IP"`
	XForwardedFor  string `header:"X-Forwarded-For"`
}

type settingsOutput struct{ Body SettingsView }

func (h *SettingsHandlers) Get(ctx context.Context, in *settingsGetInput) (*settingsOutput, error) {
	stored, err := h.d.State.Settings(ctx)
	if err != nil {
		return nil, err
	}
	if stored == nil {
		stored = map[string]any{}
	}
	values := h.d.Settings.Values(ctx)
	values.DataDir = h.d.DataDir
	hop := h.hopOf(ctx, in.CFConnectingIP != "" || in.XForwardedFor != "")
	return &settingsOutput{Body: SettingsOf(config.Of(values, stored), hop, h.d.SMBAgentView())}, nil
}

type settingsPatchInput struct {
	Section string `path:"section"`
	Body    map[string]any
}

// applyOutput answers 200 when the change was stored and 422 with the
// findings that refused it.
type applyOutput struct {
	Status int
	Body   ApplyOutcomeView
}

func (h *SettingsHandlers) Patch(ctx context.Context, in *settingsPatchInput) (*applyOutput, error) {
	section, body := in.Section, in.Body
	if !config.Known(section) {
		return nil, apierr.AsClassified(apierr.Unprocessable, "")
	}
	if err := h.extractSecrets(ctx, section, body); err != nil {
		return nil, err
	}
	var selfHost string
	if a, ok := middleware.AuthorityFrom(ctx); ok {
		selfHost = config.HostOnly(a.Host)
	}
	findings := config.Section(config.Input{
		Section:   section,
		Body:      body,
		SelfHost:  selfHost,
		DataDir:   h.d.DataDir,
		HasSecret: section == "oidc" && h.d.Settings.HasConfigSecret(ctx, secretOIDCClient),
		Lockout:   config.LockoutBlocks,
	})
	if Blocking(findings) {
		return &applyOutput{Status: http.StatusUnprocessableEntity, Body: ApplyOutcomeOf(false, false, false, findings)}, nil
	}
	if err := h.d.State.MergeSettings(ctx, section, body); err != nil {
		return nil, err
	}
	restart := config.RestartRequiredFor(section, body)
	if !restart {
		h.d.Settings.Load(ctx)
	}
	if section == "smb" && h.d.PublishSMB != nil {
		h.d.PublishSMB(ctx)
	}
	applied := !restart
	if pin := h.pinnedBindFinding(section, body); pin != nil {
		applied = false
		findings = append(findings, *pin)
	}
	out := ApplyOutcomeOf(true, applied, restart, findings)
	if restart {
		uploads, jobs := h.activeWork(ctx)
		out = out.WithActiveWork(uploads, jobs)
	}
	return &applyOutput{Status: http.StatusOK, Body: out}, nil
}

type restartOutput struct {
	Status int
	Body   restartResult
}

func (h *SettingsHandlers) Restart(ctx context.Context, _ *struct{}) (*restartOutput, error) {
	if h.d.Settings.WouldLoosenHardening(ctx, h.d.Hardening) {
		return nil, apierr.AsClassified(apierr.Conflict, "system.hardening_cannot_loosen")
	}
	uploads, jobs := h.activeWork(ctx)
	if h.d.OnRestart != nil {
		time.AfterFunc(restartGrace, h.d.OnRestart)
	}
	return &restartOutput{Status: http.StatusAccepted, Body: restartResult{Restarting: true, ActiveUploads: uploads, ActiveJobs: jobs}}, nil
}

type restartResult struct {
	Restarting    bool `json:"restarting"`
	ActiveUploads int  `json:"active_uploads"`
	ActiveJobs    int  `json:"active_jobs"`
}

func (h *SettingsHandlers) activeWork(ctx context.Context) (uploads, jobs int) {
	w, err := h.d.State.CountActiveWork(ctx)
	if err != nil {
		if h.d.Logger != nil {
			h.d.Logger.Warn("could not count the work a restart would interrupt", "error", err)
		}
		return 1, 0
	}
	return w.Uploads, w.Jobs
}

func (h *SettingsHandlers) pinnedBindFinding(section string, body map[string]any) *config.Finding {
	if section != "network" || !h.d.Settings.BindPinned() {
		return nil
	}
	stored, named := body["bind"].(string)
	if !named || stored == "" {
		return nil
	}
	return &config.Finding{Section: section, Field: "bind", ReasonKey: "settings.bind_pinned_by_flag", Args: []string{"stored", stored}}
}

// extractSecrets moves the OIDC client secret out of the body into the secret
// store, so it is never merged into the plain settings.
func (h *SettingsHandlers) extractSecrets(ctx context.Context, section string, body map[string]any) error {
	if section != "oidc" {
		return nil
	}
	raw, present := body["client_secret"]
	if !present {
		return nil
	}
	delete(body, "client_secret")
	plain, ok := raw.(string)
	if !ok {
		return apierr.AsClassified(apierr.Unprocessable, "")
	}
	return h.d.Settings.StoreConfigSecret(ctx, secretOIDCClient, plain)
}

func (h *SettingsHandlers) hopOf(ctx context.Context, forwardedSeen bool) HopView {
	hop := HopView{Client: middleware.ClientFrom(ctx).String(), ForwardedSeen: forwardedSeen}
	peer, ok := middleware.PeerFrom(ctx)
	if !ok {
		return hop
	}
	hop.Peer = peer.String()
	hop.PeerTrusted = middleware.PeerTrusted(peer, h.d.Settings.TrustedProxies())
	return hop
}
