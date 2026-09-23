//go:build linux

package app

import (
	"context"
	"log/slog"

	"github.com/gin-gonic/gin"
	hanamibootstrap "github.com/heavycaffeiner/hanami/bootstrap"
	hanamigin "github.com/heavycaffeiner/hanami/gin"
	hanamiprocess "github.com/heavycaffeiner/hanami/process"
	"github.com/heavycaffeiner/stowcloud/go/internal/platform/system/jail"
	"github.com/heavycaffeiner/stowcloud/go/internal/runtime/listener"
	runtimerestart "github.com/heavycaffeiner/stowcloud/go/internal/runtime/restart"
	"go.uber.org/fx"
)

// ModuleConfig contains the process-level values needed to construct the
// application and bind its runtime adapters.
type ModuleConfig struct {
	DataDir   string
	Address   string
	Pinned    bool
	Plain     bool
	Hardening jail.Policy
	Revision  string
	Logger    *slog.Logger
}

// Module composes the Stowcloud application graph. Feature services remain
// plain Go dependencies; Fx and Hanami stay at this application boundary.
func Module(config ModuleConfig) fx.Option {
	return fx.Options(
		hanamigin.Module(hanamigin.Config{Mode: gin.ReleaseMode}),
		fx.Provide(func(ctx context.Context) (*Engine, error) {
			return Open(ctx, Options{
				DataDir:                        config.DataDir,
				Logger:                         config.Logger,
				Hardening:                      config.Hardening,
				Revision:                       config.Revision,
				InstanceLockAcquiredExternally: true,
			})
		}),
		fx.Invoke(func(lifecycle fx.Lifecycle, engine *Engine) {
			lifecycle.Append(fx.Hook{OnStop: func(context.Context) error { return engine.Close() }})
		}),
		fx.Provide(func(engine *Engine, router *gin.Engine, admission *hanamibootstrap.Admission, controller *hanamiprocess.Controller) (*listener.Runtime, error) {
			if err := engine.Mount(router); err != nil {
				return nil, err
			}
			return listener.New(listener.Config{
				DataDir: config.DataDir,
				Address: config.Address,
				Pinned:  config.Pinned,
				Plain:   config.Plain,
				Logger:  config.Logger,
			}, engine, router, admission, controller)
		}),
		fx.Invoke(func(lifecycle fx.Lifecycle, runtime *listener.Runtime) {
			lifecycle.Append(fx.Hook{OnStart: runtime.Start, OnStop: runtime.Stop})
		}),
		fx.Invoke(func(engine *Engine, controller *hanamiprocess.Controller) {
			runtimerestart.Bind(engine, controller)
		}),
	)
}
