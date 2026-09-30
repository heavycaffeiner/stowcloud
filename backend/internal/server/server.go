//go:build linux

// Package server is the one wiring package. It opens the engine, mounts every
// route and serves them on a managed listener until the process stops.
package server

import (
	"context"
	"errors"
	"log/slog"

	"github.com/gin-gonic/gin"
	"github.com/heavycaffeiner/stowcloud/backend/internal/db/instance"
	"github.com/heavycaffeiner/stowcloud/backend/internal/platform/system/jail"
)

// Config is what the command settled before the engine exists.
type Config struct {
	DataDir      string
	Address      string
	Pinned       bool
	Plain        bool
	Hardening    jail.Policy
	Revision     string
	Logger       *slog.Logger
	InstanceLock *instance.Lock
}

// Run serves until ctx ends or the engine asks for a restart. A restart comes
// back as an error, so the process exits and its supervisor starts a new one.
// ctx must outlive startup: the engine's watchers run until it ends.
func Run(ctx context.Context, config Config) error {
	engine, err := Open(ctx, Options{
		DataDir:      config.DataDir,
		Logger:       config.Logger,
		Hardening:    config.Hardening,
		Revision:     config.Revision,
		InstanceLock: config.InstanceLock,
	})
	if err != nil {
		return err
	}
	gin.SetMode(gin.ReleaseMode)
	router := gin.New()
	if err = engine.Mount(router); err != nil {
		return errors.Join(err, engine.Close())
	}
	listener, err := NewListener(ListenerConfig{
		DataDir: config.DataDir, Address: config.Address, Pinned: config.Pinned,
		Plain: config.Plain, Logger: config.Logger,
	}, engine.Settings, router)
	if err != nil {
		return errors.Join(err, engine.Close())
	}
	engine.Restart.OnRestart(listener.RequestRestart)
	return errors.Join(listener.Serve(ctx), engine.Close())
}
