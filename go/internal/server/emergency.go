// Linux only: it depends on packages that are Linux only.
//go:build linux

package server

import (
	"context"
	"crypto/tls"
	"errors"
	"log/slog"
	"net/http"

	"github.com/heavycaffeiner/stowcloud/go/internal/auth"
	"github.com/heavycaffeiner/stowcloud/go/internal/emergency"
	"github.com/heavycaffeiner/stowcloud/go/internal/httpapi/mw"
	"github.com/heavycaffeiner/stowcloud/go/internal/httpapi/spa"
	"github.com/heavycaffeiner/stowcloud/go/internal/runtimecfg"
	"github.com/heavycaffeiner/stowcloud/go/internal/store"
)

// The emergency layer's own listener, for the case where there is no other
// one.
//
// It is the same mux the healthy server mounts under /emergency, on a server
// built from the store and the auth service alone. Nothing the engine owns is
// touched, so a setting that stops the engine coming up cannot stop this: that
// is the whole reason it exists.

// EmergencyOptions is what a standalone emergency listener needs.
type EmergencyOptions struct {
	Store *store.Store
	Auth  *auth.Service
	Log   *slog.Logger

	DataDir string

	// Reason names what failed, for the banner. Empty is the operator asking
	// for the door deliberately rather than being sent to it.
	Reason string

	// Restart asks the process to exit so a supervisor starts it again. Nil
	// leaves the action reporting that this deployment has none.
	Restart func()
}

// ServeEmergency binds the listener and serves only the emergency mux.
//
// The address is the stored one when the store yields it, so the address an
// operator already knows keeps answering; a stored value that will not parse
// falls back to the default rather than refusing to start, because a repair
// door that will not open over a bad setting is the one thing it must never
// do.
func ServeEmergency(ctx context.Context, opt EmergencyOptions) (*Serve, error) {
	log := opt.Log
	if log == nil {
		log = slog.Default()
	}
	if opt.Store == nil || opt.Auth == nil {
		return nil, errors.New("the emergency listener needs the store and the auth service")
	}
	st := opt.Store.State()
	values := runtimecfg.Load(ctx, st, runtimecfg.Defaults(), log)

	addr := values.Listen
	if runtimecfg.CheckListen(addr) != nil {
		log.Warn("the stored bind address cannot be bound; the emergency listener is using the default",
			"stored", addr, "default", runtimecfg.DefaultListen)
		addr = runtimecfg.DefaultListen
	}

	reason := opt.Reason
	page, _ := spa.Handler()
	mux := emergency.Handler(emergency.Deps{
		Auth: opt.Auth, State: st, Log: log, DataDir: opt.DataDir,
		Page:    page,
		Reason:  func() string { return reason },
		Restart: opt.Restart,
		// The stored ranges, so a deployment behind its own reverse proxy is
		// not locked out of the repair door by that proxy.
		Trusted: mw.NewTrustedSet(runtimecfg.ParsePrefixes(values.TrustedProxy)),
	})

	// Everything that is not the emergency prefix is sent to it. This process
	// has no engine, so there is nothing else here to answer with, and a
	// redirect is what turns "browse to the address you know" into the repair
	// screen.
	handler := emergency.Redirecting(mux, page, func() string { return reason })

	appHost := ""
	if len(values.AppHosts) > 0 {
		appHost = values.AppHosts[0]
	}
	build := func(string) (*http.Server, error) {
		material, err := loadOrCreateTLS(opt.DataDir, appHost)
		if err != nil {
			return nil, err
		}
		return newHTTPServer(handler, &tls.Config{
			Certificates: []tls.Certificate{*material.Cert},
			MinVersion:   tls.VersionTLS12,
			NextProtos:   []string{"h2", "http/1.1"},
		}), nil
	}
	s, err := NewServe(ctx, log, addr, build)
	if err == nil {
		return s, nil
	}
	if addr == runtimecfg.DefaultListen {
		return nil, err
	}
	// The stored address parses and still will not bind: an interface that is
	// not on this machine, or a port something else is holding. That is one of
	// the settings somebody comes to this door to fix, so the door does not
	// refuse to open over it.
	log.Warn("the stored bind address could not be bound; the emergency listener is falling back",
		"stored", addr, "fallback", runtimecfg.DefaultListen, "error", err)
	return NewServe(ctx, log, runtimecfg.DefaultListen, build)
}
