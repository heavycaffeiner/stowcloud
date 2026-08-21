package server

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"log/slog"
	"net/http"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/internal/auth"
	"github.com/heavycaffeiner/stowcloud/go/internal/clock"
	"github.com/heavycaffeiner/stowcloud/go/internal/core"
	"github.com/heavycaffeiner/stowcloud/go/internal/dav"
	"github.com/heavycaffeiner/stowcloud/go/internal/httpapi"
	"github.com/heavycaffeiner/stowcloud/go/internal/httpapi/handler"
	"github.com/heavycaffeiner/stowcloud/go/internal/httpapi/mw"
	"github.com/heavycaffeiner/stowcloud/go/internal/httpapi/route"
	"github.com/heavycaffeiner/stowcloud/go/internal/httpapi/ws"
	"github.com/heavycaffeiner/stowcloud/go/internal/smbagent"
	"github.com/heavycaffeiner/stowcloud/go/internal/store"
	"github.com/heavycaffeiner/stowcloud/go/internal/upload"
	"github.com/heavycaffeiner/stowcloud/go/internal/watch"
)

// Options is what New needs that the config does not imply: the opened
// store, the auth service with its master key, the core domain, and the
// logger. The server package owns the assembly; the command owns the
// opening.
type Options struct {
	Store *store.Store
	Auth  *auth.Service
	Core  *core.Core
	Log   *slog.Logger
	Clk   clock.Clock

	// Watch is the live watcher and WS the change-channel hub. Both are nil
	// when the mount was not assembled with a watcher (the unit tests); the
	// command wires them so /api/events upgrades real sockets.
	Watch *watch.Watcher
	WS    *ws.Hub

	// Health carries what the running server has to report as degraded. A nil
	// one is an empty one, so a test assembly reports ok rather than crashing
	// on a surface it never asked for.
	Health *handler.HealthState

	// Uploads is the resumable-upload engine. A nil one leaves the surface
	// answering that it is unavailable rather than minting sessions nothing
	// backs.
	Uploads *upload.Engine

	// PublishSMB re-renders the SMB configuration and asks the sidecar to
	// apply it. A nil one leaves the apply surface refusing rather than
	// pretending, which is what a deployment with no sidecar has.
	PublishSMB func(ctx context.Context) (smbagent.Report, error)

	// ReloadACL rebuilds the permission evaluator from the stored grants,
	// which is what makes an edit on the admin screen take effect without a
	// restart. A nil one leaves the grants as they were loaded.
	ReloadACL func(ctx context.Context) error
}

// New assembles the whole HTTP surface: the state, the route table, the
// chain, the listener's TLS configuration, and the http.Server with the
// timeouts D13 asserts. It does not bind; the command does that.
func New(cfg *Config, opt Options, setup *SetupGate) (*http.Server, error) {
	clk := opt.Clk
	if clk == nil {
		clk = clock.System()
	}
	log := opt.Log
	if log == nil {
		log = slog.Default()
	}

	state := &httpapi.State{
		Log:     log,
		Clock:   clk,
		Auth:    opt.Auth,
		Core:    opt.Core,
		Trusted: mw.NewTrustedSet(cfg.TrustedProxy),
		Hosts:   mw.NewHostSet(cfg.AppHosts, cfg.ContentHosts),
		CSRFKey: csrfKey(),
		Limiter: mw.NewRateLimiter(cfg.RatePerSec, cfg.RateBurst, clk),
	}

	health := opt.Health
	if health == nil {
		health = handler.NewHealthState()
	}

	deps := handler.Deps{
		Core:       opt.Core,
		Auth:       opt.Auth,
		Clock:      clk,
		Log:        log,
		Limiter:    state.Limiter,
		Trusted:    state.Trusted,
		Hosts:      state.Hosts,
		CSRFKey:    state.CSRFKey,
		WatchCap:   func() int { return watchHotSetCap },
		Health:     health,
		Uploads:    opt.Uploads,
		State:      opt.Store.State(),
		PublishSMB: opt.PublishSMB,
		ReloadACL: func(ctx context.Context) error {
			if opt.ReloadACL == nil {
				return nil
			}
			return opt.ReloadACL(ctx)
		},
	}

	table := routes(deps, setup)
	if err := route.Validate(table); err != nil {
		return nil, err
	}
	state.SetLookup(route.From(table))

	// The WebDAV mount. The protocol package is handed resolved paths, so the
	// mount above it is what turns a URL into one: without it the package is
	// complete and unreachable, which is how it sat until the differ asked.
	davHandler := dav.New(dav.Options{
		Core:   opt.Core,
		State:  opt.Store.State(),
		Locks:  dav.NewLocks(opt.Store.State(), clk),
		Logger: log,
	})

	m := mux(table, compatRoutes(opt.Core, opt.Store.State(), opt.Log), davMount(davHandler, opt.Core))
	handler := httpapi.Chain(state)(m)

	tlsCfg, err := tlsConfig(cfg, opt)
	if err != nil {
		return nil, err
	}

	srv := newHTTPServer(handler, tlsCfg)
	return srv, nil
}

// newHTTPServer is the one construction of the product's http.Server, split
// out so the D13 test asserts the timeout fields against the real builder
// rather than against a copy. The two zeros are deliberate: uploads and
// downloads stream, and a whole-request deadline would break them. The
// protection that replaces them is ReadHeaderTimeout for the slowloris case
// and a per-read idle deadline inside the streaming handlers.
func newHTTPServer(handler http.Handler, tlsCfg *tls.Config) *http.Server {
	return &http.Server{
		Handler:           handler,
		TLSConfig:         tlsCfg,
		ReadHeaderTimeout: 10 * time.Second, // D13: zero means no limit.
		ReadTimeout:       0,                // deliberate: uploads stream.
		WriteTimeout:      0,                // deliberate: downloads stream.
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    64 << 10,
	}
}

// csrfKey mints the per-process key session CSRF tokens derive from.
func csrfKey() []byte {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		panic("crypto/rand is unavailable: " + err.Error()) //nolint:forbidigo // no CSRF without it.
	}
	return key
}

// tlsConfig builds the server's TLS half: TLS 1.2 floor and ALPN for h2 and
// http/1.1, both of which Go negotiates when the certificate and the next
// protocols are present.
func tlsConfig(cfg *Config, opt Options) (*tls.Config, error) {
	material, err := loadOrCreateTLS(cfg.DataDir, cfg.AppHost)
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		Certificates: []tls.Certificate{*material.Cert},
		MinVersion:   tls.VersionTLS12,
		NextProtos:   []string{"h2", "http/1.1"},
	}, nil
}

// watchHotSetCap is the watcher's hot-set bound, reported by the settings
// surface. The live watcher gets its value from Phase 1's config; this is the
// constant the settings surface names.
const watchHotSetCap = 4096

// StartWatch brings up the watcher, registers every share root into it, and
// builds the change-channel hub over the same event stream. It is the one
// place the watch refcount and the WebSocket subscription meet: subscribing
// pins the directory into the sticky set and unsubscribing releases it.
func StartWatch(ctx context.Context, coreSvc *core.Core, clk clock.Clock, log *slog.Logger) (*watch.Watcher, *ws.Hub, error) {
	events := make(chan watch.InvalEvent, 64)
	w, err := watch.Start(ctx, watch.Config{}, clk, events)
	if err != nil {
		return nil, nil, err
	}
	for _, def := range coreSvc.Shares() {
		w.AddShare(def.ID, def.Host, true)
	}
	hub := ws.NewHub(ctx, coreSvc, w, clk, log, events)
	return w, hub, nil
}
