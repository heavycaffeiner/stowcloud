//go:build linux

package server

import (
	"context"
	"log/slog"

	"github.com/gin-gonic/gin"
	hanamibootstrap "github.com/heavycaffeiner/hanami/bootstrap"
	hanamigin "github.com/heavycaffeiner/hanami/gin"
	hanamiprocess "github.com/heavycaffeiner/hanami/process"
	"github.com/heavycaffeiner/stowcloud/backend/internal/db/instance"
	"github.com/heavycaffeiner/stowcloud/backend/internal/platform/system/jail"
	"go.uber.org/fx"
)

// ModuleConfig contains the process-level values needed to construct the
// application and bind its runtime adapters.
type ModuleConfig struct {
	DataDir      string
	Address      string
	Pinned       bool
	Plain        bool
	Hardening    jail.Policy
	Revision     string
	Logger       *slog.Logger
	InstanceLock *instance.Lock
}

// Module composes the Stowcloud application graph. Feature services remain
// plain Go dependencies; Fx and Hanami stay at this application boundary.
func Module(config ModuleConfig) fx.Option {
	return fx.Options(
		hanamigin.Module(hanamigin.Config{Mode: gin.ReleaseMode}),
		// The context fx supplies expires when startup's deadline does. Open
		// hands its context to the storage watcher and the membership reload,
		// which must outlive startup, so it gets the process's own.
		fx.Provide(func() (*Engine, error) {
			return Open(context.Background(), Options{
				DataDir:                        config.DataDir,
				Logger:                         config.Logger,
				Hardening:                      config.Hardening,
				Revision:                       config.Revision,
				InstanceLockAcquiredExternally: false,
				InstanceLock:                   config.InstanceLock,
			})
		}),
		fx.Invoke(func(lifecycle fx.Lifecycle, engine *Engine) {
			lifecycle.Append(fx.Hook{OnStop: func(context.Context) error { return engine.Close() }})
		}),
		fx.Provide(func(engine *Engine, router *gin.Engine, admission *hanamibootstrap.Admission, controller *hanamiprocess.Controller) (*Listener, error) {
			if err := engine.Mount(router); err != nil {
				return nil, err
			}
			return NewListener(ListenerConfig{
				DataDir: config.DataDir, Address: config.Address, Pinned: config.Pinned,
				Plain: config.Plain, Logger: config.Logger,
			}, engine.Settings, router, admission, controller)
		}),
		fx.Invoke(func(lifecycle fx.Lifecycle, runtime *Listener) {
			lifecycle.Append(fx.Hook{OnStart: runtime.Start, OnStop: runtime.Stop})
		}),
		fx.Invoke(func(engine *Engine, controller *hanamiprocess.Controller) {
			bindRestart(engine.Restart, controller)
		}),
	)
}
