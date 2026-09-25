//go:build linux

// SMB publication composition. Rendering, grant mapping, and publication state
// belong to feature/smb/publish; this file only wires application dependencies.
package app

import (
	"context"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/internal/feature/admin/settings/runtimecfg"
	"github.com/heavycaffeiner/stowcloud/go/internal/feature/smb"
	"github.com/heavycaffeiner/stowcloud/go/internal/feature/smb/agent"
	"github.com/heavycaffeiner/stowcloud/go/internal/feature/smb/publish"
)

const publishTimeout = agent.DefaultTimeout + 5*time.Second

type smbSettings struct {
	Config     smb.Config
	ConfigDir  string
	Socket     string
	GID        uint32
	Configured bool
}

func smbSettingsOf(ctx context.Context, e *Engine) smbSettings {
	values := runtimecfg.Load(ctx, e.State, runtimecfg.Defaults(), e.logger)
	return smbSettings{Config: values.SMB, ConfigDir: values.SMBConfigDir, Socket: values.SMBSocket, GID: values.SMBServiceGID, Configured: values.SMBConfigured}
}

func newSMBPublisher(e *Engine, s smbSettings) *publish.Publisher {
	if !s.Configured || s.ConfigDir == "" {
		return nil
	}
	return publish.New(publish.PublisherDeps{
		Core:   e.Core,
		Auth:   e.Auth,
		State:  e.State,
		Clock:  e.clk(),
		Logger: e.log(),
		Settings: func(ctx context.Context) publish.Settings {
			current := smbSettingsOf(ctx, e)
			return publish.Settings{Config: current.Config, ConfigDir: current.ConfigDir, Socket: current.Socket, ServiceGID: current.GID, Configured: current.Configured}
		},
	})
}

func (e *Engine) publishSMBAtBoot(ctx context.Context) {
	if e.smb == nil {
		return
	}
	if _, err := e.smb.Publish(ctx); err != nil {
		e.log().Warn("the SMB configuration could not be published at startup", "error", err)
	}
}

func (e *Engine) smbPublisherOf() *publish.Publisher {
	e.settingsMu.RLock()
	defer e.settingsMu.RUnlock()
	return e.smb
}

func (e *Engine) publishSMBSettings(ctx context.Context) {
	e.settingsMu.Lock()
	if e.smb == nil {
		if p := newSMBPublisher(e, smbSettingsOf(ctx, e)); p != nil {
			e.smb = p
			e.Auth.SetAccessChangeSink(p)
		}
	}
	p := e.smb
	e.settingsMu.Unlock()
	if p == nil {
		return
	}
	pctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), publishTimeout)
	defer cancel()
	report, err := p.Publish(pctx)
	switch {
	case err != nil:
		e.log().Warn("a file-sharing settings change did not reach the SMB sidecar", "error", err)
	case !report.OK:
		e.log().Warn("the SMB sidecar applied the settings change with a warning", "error", report.Error)
	}
}
