// Linux only: it depends on packages that are Linux only.
//go:build linux

package server

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"errors"
	"log/slog"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/internal/auth"
	"github.com/heavycaffeiner/stowcloud/go/internal/clock"
	"github.com/heavycaffeiner/stowcloud/go/internal/core"
	"github.com/heavycaffeiner/stowcloud/go/internal/dav"
	"github.com/heavycaffeiner/stowcloud/go/internal/emergency"
	"github.com/heavycaffeiner/stowcloud/go/internal/httpapi"
	"github.com/heavycaffeiner/stowcloud/go/internal/httpapi/handler"
	"github.com/heavycaffeiner/stowcloud/go/internal/httpapi/mw"
	"github.com/heavycaffeiner/stowcloud/go/internal/httpapi/route"
	"github.com/heavycaffeiner/stowcloud/go/internal/httpapi/spa"
	"github.com/heavycaffeiner/stowcloud/go/internal/httpapi/ws"
	"github.com/heavycaffeiner/stowcloud/go/internal/oidc"
	"github.com/heavycaffeiner/stowcloud/go/internal/preview"
	"github.com/heavycaffeiner/stowcloud/go/internal/runtimecfg"
	"github.com/heavycaffeiner/stowcloud/go/internal/search/service"
	"github.com/heavycaffeiner/stowcloud/go/internal/smbagent"
	"github.com/heavycaffeiner/stowcloud/go/internal/store"
	"github.com/heavycaffeiner/stowcloud/go/internal/task"
	"github.com/heavycaffeiner/stowcloud/go/internal/upload"
	"github.com/heavycaffeiner/stowcloud/go/internal/watch"
)

// eventsHandler is the change channel's upgrade, or nil when no hub was wired.
//
// Nil rather than a handler that fails: a typed nil behind an interface is not
// nil, so returning the method value of a nil hub would make the surface look
// present and answer with a panic.
func eventsHandler(hub *ws.Hub) handler.EventsHandler {
	if hub == nil {
		return nil
	}
	return hub.Upgrade
}

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

	// OIDC is the single-sign-on client, or nil when none is configured.
	OIDC *oidc.Client

	// Preview generates thumbnails. A nil one leaves the listing reporting
	// that no entry has one, and the route answering 404.
	Preview *preview.Service

	// Search answers queries and builds the name index. A nil one leaves every
	// query taking the walk, which is the correct behaviour for a build with
	// no index.
	Search *service.Service

	// ApplyIndexEnabled attaches or detaches the index in the running process,
	// so the administrator's switch takes effect without a restart. A nil one
	// leaves the settings surface reporting that a restart is needed, which is
	// honest rather than a success that changes nothing.
	ApplyIndexEnabled func(enabled bool) error

	// Runtime holds the settings an administrator moves from the interface,
	// already loaded from the store. A nil one leaves every field reported and
	// none of them editable.
	Runtime *runtimecfg.Holder

	// PublishSMB re-renders the SMB configuration and asks the sidecar to
	// apply it. A nil one leaves the apply surface refusing rather than
	// pretending, which is what a deployment with no sidecar has.
	PublishSMB func(ctx context.Context) (smbagent.Report, error)

	// ReloadACL rebuilds the permission evaluator from the stored grants,
	// which is what makes an edit on the admin screen take effect without a
	// restart. A nil one leaves the grants as they were loaded.
	ReloadACL func(ctx context.Context) error

	// StoreSecret seals a settings value that is a credential. It comes from
	// the command, which is the layer holding the master key.
	StoreSecret func(ctx context.Context, plain string) error

	// HasOIDCSecret reports whether a client secret is stored, which is all
	// the settings screen is ever told about it.
	HasOIDCSecret bool

	// RequestRestart asks the process to exit so a supervisor starts it again.
	// It is what a saved setting the sandbox pins reaches. A nil one leaves
	// those saves reporting the value as stored and not in effect, which is
	// honest rather than a success that changed nothing.
	RequestRestart func()

	// ActiveWork reports what a restart would interrupt, so the refusal can
	// name the counts an administrator decides from.
	ActiveWork func() handler.ActiveWork

	// PathInJail reports whether a host path is inside the sandbox's domain,
	// which is what tells a new share that is reachable now from one that
	// needs the process rebuilt.
	PathInJail func(host string) bool
}

// New assembles the whole HTTP surface once and returns what serves it.
//
// The Serve it hands back owns the listener and can move it: the request
// state, the route table and the chain are built here and reused, and a swap
// rebuilds only the http.Server and its TLS configuration. That is the whole
// of what a bind address decides, so nothing under it has to be rebuilt to
// move a socket.
func New(ctx context.Context, cfg *Config, opt Options, setup *SetupGate) (*Serve, error) {
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
		Hosts:   mw.NewHostSet(cfg.AppHosts),
		CSRFKey: csrfKey(),
		Limiter: mw.NewRateLimiter(cfg.RatePerSec, cfg.RateBurst, clk),
		// The policy has to admit this bundle's own inline bootstrap, and the
		// hashes are read from the bundle rather than written down: a constant
		// is a second copy of what the build already decided, and the two
		// disagreeing is a blank page whose only symptom is a console line.
		AppCSP: mw.AppPolicy(spa.InlineScriptHashes()),
	}

	health := opt.Health
	if health == nil {
		health = handler.NewHealthState()
	}

	// The listener does not exist yet: it is built below, from the table this
	// is about to go into. The handlers reach it through this rather than
	// through a copy of a value taken before it existed, which is what a
	// captured field would have been.
	var serve *Serve

	deps := handler.Deps{
		Listen: func() string {
			if serve == nil {
				return cfg.Listen
			}
			return serve.Addr()
		},
		SwapListener: func(c context.Context, addr string) error {
			if serve == nil {
				return errors.New("the listener is not running yet")
			}
			return serve.Swap(c, addr)
		},
		Core:              opt.Core,
		Auth:              opt.Auth,
		Clock:             clk,
		Log:               log,
		Limiter:           state.Limiter,
		Trusted:           state.Trusted,
		Hosts:             state.Hosts,
		CSRFKey:           state.CSRFKey,
		WatchCap:          func() int { return watchHotSetCap },
		Runtime:           opt.Runtime,
		SMBConfigDir:      cfg.SMB.ConfigDir,
		DataDir:           cfg.DataDir,
		Health:            health,
		Uploads:           opt.Uploads,
		Preview:           opt.Preview,
		State:             opt.Store.State(),
		Search:            opt.Search,
		OIDC:              opt.OIDC,
		OIDCDisplayName:   cfg.OIDCDisplayName,
		Events:            eventsHandler(opt.WS),
		PublishSMB:        opt.PublishSMB,
		SMBChanged:        smbSink(opt.PublishSMB, cfg.SMB.AgentSocket, health, log),
		ApplyIndexEnabled: opt.ApplyIndexEnabled,
		StoreSecret:       opt.StoreSecret,
		HasOIDCSecret:     opt.HasOIDCSecret,
		RequestRestart:    opt.RequestRestart,
		ActiveWork:        opt.ActiveWork,
		PathInJail:        opt.PathInJail,
		ReloadACL: func(ctx context.Context) error {
			if opt.ReloadACL == nil {
				return nil
			}
			return opt.ReloadACL(ctx)
		},
	}

	// What a saved setting reaches. Installed here because this is the layer
	// that owns the live components: the limiter is built above and the search
	// service is handed in, and the command that assembles both cannot reach
	// the first one.
	//
	// The watcher's bound is deliberately absent: it is taken when the watcher
	// starts, so that field reports restart_required rather than being pushed
	// somewhere it would not take effect.
	if opt.Runtime != nil {
		opt.Runtime.OnApply(func(v runtimecfg.Values) {
			state.Limiter.Set(v.RatePerSec, v.RateBurst)
			if opt.Search != nil {
				opt.Search.SetBounds(v.SearchConcurrentSSD, v.SearchDeadlineSSD)
			}
			// The host lists and the proxy ranges are live: the guard reads
			// them per request, so a saved change takes effect on the next
			// one rather than the next start. An entry that cannot be parsed
			// is dropped with a line here and refused at save time, which is
			// the pair of rules the proxy boundary is meant to have.
			if len(v.AppHosts) > 0 {
				state.Hosts.Set(v.AppHosts)
			}
			if len(v.TrustedProxy) > 0 {
				state.Trusted.Set(parsePrefixes(v.TrustedProxy, log))
			}
			// The healthcheck is a second process and cannot read the
			// database this server holds, so the two facts it needs are
			// written out whenever they move. A failure is a line, not a
			// refusal: the server is serving either way.
			probe := Probe{Listen: v.Listen}
			if len(v.AppHosts) > 0 {
				probe.AppHost = v.AppHosts[0]
			}
			if perr := WriteProbe(cfg.DataDir, probe); perr != nil {
				log.Warn("the healthcheck snapshot could not be written", "error", perr)
			}
		})
		// Applied once at startup, so the values loaded from the store are in
		// the components before the first request rather than after the first
		// save.
		opt.Runtime.Set(opt.Runtime.Get())
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

	protocol, davAliases := compatPaths()
	state.Protocol = protocol
	m := mux(table,
		compatRoutes(opt.Core, opt.Store.State(), opt.Auth, originOf(cfg), clk, opt.Log, state),
		davMount(davHandler, opt.Core, davAliases), davAliases)
	handler := httpapi.Chain(state)(m)

	// The emergency door, always mounted.
	//
	// It exists on a healthy server because the moment it is needed is not the
	// moment to be starting things: an operator who has just locked themselves
	// out of the interface has no way to add a route to it. It runs outside the
	// chain, which is deliberate. The chain's host guard is one of the things
	// this door is for repairing, and a repair screen behind the guard it
	// repairs is a screen nobody can reach.
	//
	// What it is guarded by instead is the peer address, resolved from the
	// same live proxy set the chain reads, so the two entrances cannot end up
	// with different opinions about who a request is from.
	emergencyPage, _ := spa.Handler()
	emergencyMux := emergency.Handler(emergency.Deps{
		Auth: opt.Auth, State: opt.Store.State(), Log: log, DataDir: cfg.DataDir,
		Page: emergencyPage,
		// Healthy: there is nothing to put in the banner, because nobody was
		// sent here. The degraded case has no engine at all and is served by
		// the standalone layer, which owns its own reason.
		Reason:  func() string { return "" },
		Restart: opt.RequestRestart,
		Trusted: state.Trusted,
	})
	handler = emergencyDoor(emergencyMux, handler)

	// Rebuilt per listener rather than captured, because the certificate is
	// part of what a swap can be moving: an app host saved beside the bind
	// address changes which name the certificate has to carry.
	build := func(string) (*http.Server, error) {
		tlsCfg, terr := tlsConfig(cfg, opt)
		if terr != nil {
			return nil, terr
		}
		return newHTTPServer(handler, tlsCfg), nil
	}

	s, err := NewServe(ctx, log, cfg.Listen, build)
	if err != nil {
		return nil, err
	}
	serve = s
	return serve, nil
}

// emergencyDoor claims the one prefix and passes everything else through.
func emergencyDoor(mux, normal http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, emergency.Prefix) {
			mux.ServeHTTP(w, r)
			return
		}
		normal.ServeHTTP(w, r)
	})
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

// parsePrefixes turns the stored proxy ranges into prefixes.
//
// An entry that cannot be parsed is dropped with a line rather than failing
// the load: the same value is refused at save time, where an administrator is
// watching, and refusing it here would make a server unbootable over one saved
// weeks ago.
func parsePrefixes(cidrs []string, log *slog.Logger) []netip.Prefix {
	out := make([]netip.Prefix, 0, len(cidrs))
	for _, c := range cidrs {
		p, err := netip.ParsePrefix(strings.TrimSpace(c))
		if err != nil {
			log.Warn("a stored trusted-proxy range could not be parsed and was dropped",
				"range", c, "error", err)
			continue
		}
		out = append(out, p)
	}
	return out
}

// originOf is the base URL the compatibility layer hands to a client.
//
// The first declared app host, over https because that is the only listener
// this server has. It is configuration rather than the request's own Host: a
// login URL is opened in a browser minutes later and mailed between devices,
// so the set of names it can carry is fixed by the operator rather than by
// whoever asked.
func originOf(cfg *Config) string {
	if cfg.AppHost == "" {
		return ""
	}
	return "https://" + cfg.AppHost
}

// smbSink turns the publisher into the form every write path calls.
//
// It exists because SMB used to be republished by exactly one thing: an
// administrator pressing apply. A grant revoked, an account disabled or a
// share removed reached this server and not the daemon, so access stayed live
// over SMB until somebody happened to press it. That failed in the permissive
// direction and said nothing, which is the worst pair of properties a
// revocation path can have.
//
// Three decisions are load-bearing here.
//
// The caller is never failed. The database write already committed and this
// server is already enforcing it, so a refusal would report a change that did
// happen as one that did not. A sidecar that did not answer is a degradation
// on the health endpoint instead, which is a thing an operator monitors.
//
// The publish is synchronous. It is the caller waiting on a revocation
// reaching the other surface, which is exactly the wait that should be theirs
// rather than a background task's, and these are administrator writes rather
// than a request path.
//
// The context is detached from the request. A browser that navigated away
// mid-publish must not cancel a revocation that is halfway to the sidecar.
//
// What it returns is the same fact it degrades health with, handed to the
// caller so their own response can carry it. The caller still never fails:
// what changes is that an administrator learns at the moment they press save
// that the change did not reach the daemon, instead of on the health page
// whenever they next look at it.
func smbSink(
	publish func(context.Context) (smbagent.Report, error),
	socket string, health *handler.HealthState, log *slog.Logger,
) func(context.Context) handler.SMBOutcome {
	if publish == nil {
		// No sidecar in this deployment. A sink that does nothing is correct
		// here, and it is the one case where nothing is the right answer.
		return nil
	}
	return func(ctx context.Context) handler.SMBOutcome {
		pctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), smbPublishTimeout)
		defer cancel()

		report, err := publish(pctx)
		switch {
		case err != nil:
			log.Warn("a change did not reach the SMB sidecar", "error", err, "socket", socket)
			health.Degrade(handler.ReasonSMBStale, "publish_failed")
			// The socket is named, which is the difference between "rendered
			// and nothing applied it" and "the agent answered with a failure".
			// They are different things to go and look at.
			return handler.SMBOutcome{State: handler.SMBUnreachable, Socket: socket}
		case !report.OK:
			// The files were promoted and something in them is wrong: a share
			// path that does not exist where the daemon runs, or an account
			// the import produced no credential for. Both are an operator's to
			// fix and neither is this request's failure.
			log.Warn("the SMB sidecar applied a change with a warning", "error", report.Error)
			health.Degrade(handler.ReasonSMBStale, "applied_with_warnings")
			return handler.SMBOutcomeOf(handler.SMBWarnings, report)
		default:
			health.Resolve(handler.ReasonSMBStale, "publish_failed")
			health.Resolve(handler.ReasonSMBStale, "applied_with_warnings")
			return handler.SMBOutcomeOf(handler.SMBApplied, report)
		}
	}
}

// smbPublishTimeout bounds one push. It is the agent's own timeout plus room
// for rendering four files, so this never cuts off a call the agent is still
// answering.
const smbPublishTimeout = smbagent.DefaultTimeout + 5*time.Second

// watchHotSetCap is the watcher's hot-set bound, reported by the settings
// surface. The live watcher gets its value from Phase 1's config; this is the
// constant the settings surface names.
const watchHotSetCap = 4096

// StartWatch brings up the watcher, registers every share root into it, and
// builds the change-channel hub over the same event stream. It is the one
// place the watch refcount and the WebSocket subscription meet: subscribing
// pins the directory into the sticky set and unsubscribing releases it.
//
// observe, when set, receives every event alongside the hub. It is how the
// search index learns what changed: the index used to be filled once and never
// updated, so a file created after a build was absent from every result the
// index answered, with nothing saying the result was short.
//
// The fan-out is one task rather than two consumers of one channel, because a
// channel with two readers gives each event to exactly one of them: the hub
// and the index would each see about half the changes.
func StartWatch(
	ctx context.Context, coreSvc *core.Core, clk clock.Clock, log *slog.Logger,
	cfg watch.Config, observe func(watch.InvalEvent),
) (*watch.Watcher, *ws.Hub, error) {
	events := make(chan watch.InvalEvent, 64)
	// The bounds an administrator saved, which the watcher takes once here.
	// That is why the settings screen reports them as needing a restart rather
	// than pretending a save moves a watcher already running.
	w, err := watch.Start(ctx, cfg, clk, events)
	if err != nil {
		return nil, nil, err
	}
	for _, def := range coreSvc.Shares() {
		w.AddShare(def.ID, def.Host, true)
	}

	sink := events
	if observe != nil {
		// The hub reads this one and the fan-out below feeds it. observe is
		// called on the fan-out's own goroutine and must not block, which is
		// why the updater's own entry point drops rather than waits.
		forward := make(chan watch.InvalEvent, 64)
		sink = forward
		task.Go(ctx, "watch fan-out", func() {
			defer close(forward)
			for ev := range events {
				observe(ev)
				select {
				case forward <- ev:
				case <-ctx.Done():
					return
				}
			}
		})
	}

	hub := ws.NewHub(ctx, coreSvc, w, clk, log, sink)
	return w, hub, nil
}
